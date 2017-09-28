package pipeline

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/Sirupsen/logrus"
	"github.com/mweagle/Sparta"
	spartaAWS "github.com/mweagle/Sparta/aws"
	spartaCF "github.com/mweagle/Sparta/aws/cloudformation"
	spartaIAM "github.com/mweagle/Sparta/aws/iam"
	spartaS3 "github.com/mweagle/Sparta/aws/s3"
	gocf "github.com/mweagle/go-cloudformation"
)

// ProvisionOptions are the command line options necessary to provision
// the CloudFormation backed CodeBuild pipeline for this project
type ProvisionOptions struct {
	Noop             bool
	S3Bucket         string `validate:"required"`
	PipelineName     string `validate:"required"`
	GithubRepo       string `validate:"required"`
	GithubOAuthToken string `validate:"required"`
}

// AssumePolicyCodeBuildRoleDocument defines common a IAM::Role PolicyDocument
// used as part of IAM::Role resource definitions
var AssumePolicyCodeBuildRoleDocument = sparta.ArbitraryJSONObject{
	"Version": "2012-10-17",
	"Statement": []sparta.ArbitraryJSONObject{
		{
			"Effect": "Allow",
			"Principal": sparta.ArbitraryJSONObject{
				"Service": []string{"codebuild.amazonaws.com"},
			},
			"Action": []string{"sts:AssumeRole"},
		},
	},
}

// AssumePolicyPipelineRoleDocument is the AssumeRole document
// for the CodePipeline role
var AssumePolicyPipelineRoleDocument = sparta.ArbitraryJSONObject{
	"Version": "2012-10-17",
	"Statement": []sparta.ArbitraryJSONObject{
		{
			"Effect": "Allow",
			"Principal": sparta.ArbitraryJSONObject{
				"Service": []string{"codepipeline.amazonaws.com"},
			},
			"Action": []string{"sts:AssumeRole"},
		},
	},
}

// AssumePolicyCFNRoleDocument is the AssumeRole document for the
// CloudFormation role
var AssumePolicyCFNRoleDocument = sparta.ArbitraryJSONObject{
	"Version": "2012-10-17",
	"Statement": []sparta.ArbitraryJSONObject{
		{
			"Effect": "Allow",
			"Principal": sparta.ArbitraryJSONObject{
				"Service": []string{"cloudformation.amazonaws.com"},
			},
			"Action": []string{"sts:AssumeRole"},
		},
	},
}

// Provision is responsible for provisioning/updating the CloudFormation stack
// that builds out the CI/CD pipeline
func Provision(provisionOptions *ProvisionOptions) error {
	logger, loggerErr := sparta.NewLogger("info")
	if loggerErr != nil {
		return loggerErr
	}
	awsSession := spartaAWS.NewSession(logger)
	repoURL, repoURLErr := url.Parse(provisionOptions.GithubRepo)
	if repoURLErr != nil {
		return repoURLErr
	}

	// Split the path to get the various parts...If there are more than 3,
	// the last one is the branchname
	pathParts := strings.Split(repoURL.Path, "/")
	ghBranch := "master"
	if len(pathParts) > 3 {
		ghBranch = pathParts[len(pathParts)-1]
	}
	ghOwner := pathParts[1]
	ghRepo := pathParts[2]

	logger.WithFields(logrus.Fields{
		"Owner":  ghOwner,
		"Repo":   ghRepo,
		"Branch": ghBranch,
	}).Info("Provisioning pipeline for GitHub source")

	// Let's build a template!
	cfTemplate := gocf.NewTemplate()

	// Start with some parameters
	cfTemplate.Parameters["PipelineName"] = &gocf.Parameter{
		Type: "String",
		Description: fmt.Sprintf("Provision the %s service",
			sparta.OptionsGlobal.ServiceName),
		Default: provisionOptions.PipelineName,
	}
	cfTemplate.Parameters["GitHubOAuthToken"] = &gocf.Parameter{
		Type:        "String",
		Description: fmt.Sprintf("Create a token with 'repo' and 'admin:repo_hook' permissions here https://github.com/settings/tokens"),
		Default:     provisionOptions.GithubOAuthToken,
		NoEcho:      gocf.Bool(true),
	}
	cfTemplate.Parameters["GitHubUser"] = &gocf.Parameter{
		Type:        "String",
		Description: fmt.Sprintf("GitHub username"),
		Default:     ghOwner,
	}
	cfTemplate.Parameters["GitHubRepoName"] = &gocf.Parameter{
		Type:        "String",
		Description: fmt.Sprintf("GitHub repository name that should be monitored for changes"),
		Default:     ghRepo,
	}
	cfTemplate.Parameters["GitHubBranch"] = &gocf.Parameter{
		Type:        "String",
		Description: fmt.Sprintf("GitHub branch to monitored"),
		Default:     ghBranch,
	}
	cfTemplate.Parameters["TemplateFileName"] = &gocf.Parameter{
		Type:        "String",
		Description: fmt.Sprintf("The file name of the Sparta template"),
		Default:     "cloudformation.json",
	}
	// Test Stack
	cfTemplate.Parameters["TestStackName"] = &gocf.Parameter{
		Type: "String",
		Description: fmt.Sprintf("Test %s service stack",
			sparta.OptionsGlobal.ServiceName),
		Default: fmt.Sprintf("Test-%s-%s",
			sparta.OptionsGlobal.ServiceName,
			ghBranch),
	}
	cfTemplate.Parameters["TestStackConfig"] = &gocf.Parameter{
		Type: "String",
		Description: fmt.Sprintf("The configuration file name for the Test %s stack",
			sparta.OptionsGlobal.ServiceName),
		Default: "test.json",
	}
	// Production Stack
	cfTemplate.Parameters["ProdStackName"] = &gocf.Parameter{
		Type: "String",
		Description: fmt.Sprintf("Production %s service stack",
			sparta.OptionsGlobal.ServiceName),
		Default: fmt.Sprintf("Prod-%s-%s",
			sparta.OptionsGlobal.ServiceName,
			ghBranch),
	}
	cfTemplate.Parameters["ProdStackConfig"] = &gocf.Parameter{
		Type: "String",
		Description: fmt.Sprintf("The configuration file name for the Production %s stack",
			sparta.OptionsGlobal.ServiceName),
		Default: "production.json",
	}
	cfTemplate.Parameters["ChangeSetName"] = &gocf.Parameter{
		Type: "String",
		Description: fmt.Sprintf("A name for the production %s stack ChangeSet",
			sparta.OptionsGlobal.ServiceName),
		Default: fmt.Sprintf("UpdatePreview-%s", sparta.OptionsGlobal.ServiceName),
	}

	//////////////////////////////////////////////////////////////////////////////
	/*
	  _  _
	 | \| |__ _ _ __  ___ ___
	 | .` / _` | '  \/ -_|_-<
	 |_|\_\__,_|_|_|_\___/__/
	*/
	//////////////////////////////////////////////////////////////////////////////
	productionStackName := fmt.Sprintf("Prod-%s", sparta.OptionsGlobal.ServiceName)
	productionChangeSetName := fmt.Sprintf("ProdChangeSet-%s", sparta.OptionsGlobal.ServiceName)

	logger.WithFields(logrus.Fields{
		"StackName":     productionStackName,
		"ChangeSetName": productionChangeSetName,
	}).Info("CloudFormation pipeline information")

	artifactS3BucketResource := sparta.CloudFormationResourceName("S3ArtifactBucket",
		"S3ArtifactBucket")
	cfnRoleResource := sparta.CloudFormationResourceName("CloudFormationRole",
		"CloudFormationRole")
	codeBuildRoleResource := sparta.CloudFormationResourceName("CodeBuildRole",
		"CodeBuildRole")
	codePipelineRoleResource := sparta.CloudFormationResourceName("CodePipelineRole",
		"CodePipelineRole")
	codeBuildProjectResource := sparta.CloudFormationResourceName("CodeBuildProject",
		"CodeBuildProject")

	//////////////////////////////////////////////////////////////////////////////
	/*
	  ___ ____  ___         _       _
	 / __|__ / | _ )_  _ __| |_____| |_
	 \__ \|_ \ | _ \ || / _| / / -_)  _|
	 |___/___/ |___/\_,_\__|_\_\___|\__|
	*/
	//////////////////////////////////////////////////////////////////////////////

	s3Bucket := &gocf.S3Bucket{
		VersioningConfiguration: &gocf.S3BucketVersioningConfiguration{
			Status: gocf.String("Enabled"),
		},
	}
	s3BucketResource := cfTemplate.AddResource(artifactS3BucketResource, s3Bucket)
	s3BucketResource.DeletionPolicy = "Retain"

	//////////////////////////////////////////////////////////////////////////////
	/*
	  ___   _   __  __   ___     _
	 |_ _| /_\ |  \/  | | _ \___| |___ ___
	  | | / _ \| |\/| | |   / _ \ / -_|_-<
	 |___/_/ \_\_|  |_| |_|_\___/_\___/__/
	*/
	//////////////////////////////////////////////////////////////////////////////

	// CloudFormation Role
	cfnRoleStatements := []spartaIAM.PolicyStatement{
		spartaIAM.PolicyStatement{
			Action:   []string{"lambda:*", "iam:*"},
			Effect:   "Allow",
			Resource: gocf.String("*"),
		},
		spartaIAM.PolicyStatement{
			Action:   []string{"s3:Get*"},
			Effect:   "Allow",
			Resource: gocf.String("*"),
		},
	}
	cfnRole := &gocf.IAMRole{
		AssumeRolePolicyDocument: AssumePolicyCFNRoleDocument,
		Policies: &gocf.IAMRolePolicyList{
			gocf.IAMRolePolicy{
				PolicyName: gocf.String("CloudFormationRole"),
				PolicyDocument: sparta.ArbitraryJSONObject{
					"Version":   "2012-10-17",
					"Statement": cfnRoleStatements,
				},
			},
		},
	}
	cfTemplate.AddResource(cfnRoleResource, cfnRole)

	// CodeBuild Role
	codebuildRoleStatements := []spartaIAM.PolicyStatement{
		spartaIAM.PolicyStatement{
			Action:   []string{"s3:Get*", "s3:Put*"},
			Effect:   "Allow",
			Resource: gocf.String("*"),
		},
		spartaIAM.PolicyStatement{
			Action:   []string{"logs:*", "codebuild:*"},
			Effect:   "Allow",
			Resource: gocf.String("*"),
		},
	}
	codebuildRole := &gocf.IAMRole{
		Path: gocf.String("/"),
		AssumeRolePolicyDocument: AssumePolicyCodeBuildRoleDocument,
		Policies: &gocf.IAMRolePolicyList{
			gocf.IAMRolePolicy{
				PolicyName: gocf.String("CodeBuildRole"),
				PolicyDocument: sparta.ArbitraryJSONObject{
					"Version":   "2012-10-17",
					"Statement": codebuildRoleStatements,
				},
			},
		},
	}
	cfTemplate.AddResource(codeBuildRoleResource, codebuildRole)

	// CodePipeline Role
	codepipelineRoleStatements := []spartaIAM.PolicyStatement{
		spartaIAM.PolicyStatement{
			Action: []string{"s3:*",
				"cloudformation:CreateStack",
				"cloudformation:DescribeStacks",
				"cloudformation:DeleteStack",
				"cloudformation:UpdateStack",
				"cloudformation:CreateChangeSet",
				"cloudformation:ExecuteChangeSet",
				"cloudformation:DeleteChangeSet",
				"cloudformation:DescribeChangeSet",
				"cloudformation:SetStackPolicy",
				"iam:PassRole",
				"sns:Publish",
				"codebuild:StartBuild",
				"codebuild:BatchGetBuilds"},
			Effect:   "Allow",
			Resource: gocf.String("*"),
		},
	}
	codepipelineRole := &gocf.IAMRole{
		AssumeRolePolicyDocument: AssumePolicyPipelineRoleDocument,
		Policies: &gocf.IAMRolePolicyList{
			gocf.IAMRolePolicy{
				PolicyName: gocf.String("CodePipelineAccess"),
				PolicyDocument: sparta.ArbitraryJSONObject{
					"Version":   "2012-10-17",
					"Statement": codepipelineRoleStatements,
				},
			},
		},
	}
	cfTemplate.AddResource(codePipelineRoleResource, codepipelineRole)

	//////////////////////////////////////////////////////////////////////////////
	/*
			 ___         _     ___      _ _    _
			/ __|___  __| |___| _ )_  _(_) |__| |
		 | (__/ _ \/ _` / -_) _ \ || | | / _` |
			\___\___/\__,_\___|___/\_,_|_|_\__,_|
	*/
	//////////////////////////////////////////////////////////////////////////////
	// Get the golang version from the output:
	runtimeVersion := runtime.Version()
	golangVersionRE := regexp.MustCompile(`go(\d+\.\d+(\.\d+)?)`)
	matches := golangVersionRE.FindStringSubmatch(runtimeVersion)
	if len(matches) < 2 {
		return fmt.Errorf("Unable to determine Go version from runtime: %s", runtimeVersion)
	}

	codeBuildProject := &gocf.CodeBuildProject{
		Name:             gocf.String(fmt.Sprintf("CodeBuild-%s", sparta.OptionsGlobal.ServiceName)),
		Description:      gocf.String("Builds and deploys the service"),
		ServiceRole:      gocf.GetAtt(codeBuildRoleResource, "Arn"),
		TimeoutInMinutes: gocf.Integer(10),
		Source: &gocf.CodeBuildProjectSource{
			Type: gocf.String("CODEPIPELINE"),
		},
		Artifacts: &gocf.CodeBuildProjectArtifacts{
			Type:          gocf.String("CODEPIPELINE"),
			NamespaceType: gocf.String("NONE"),
			Name:          gocf.String("BuiltApplication"),
			Packaging:     gocf.String("NONE"),
		},
		Environment: &gocf.CodeBuildProjectEnvironment{
			Type:           gocf.String("LINUX_CONTAINER"),
			Image:          gocf.String(fmt.Sprintf("golang:%s", matches[1])),
			ComputeType:    gocf.String("BUILD_GENERAL1_SMALL"),
			PrivilegedMode: gocf.Bool(false),
		},
	}
	cfTemplate.AddResource(codeBuildProjectResource, codeBuildProject)

	//////////////////////////////////////////////////////////////////////////////
	/*
	  ___ _           _ _
	 | _ (_)_ __  ___| (_)_ _  ___
	 |  _/ | '_ \/ -_) | | ' \/ -_)
	 |_| |_| .__/\___|_|_|_||_\___|
	       |_|
	*/
	//////////////////////////////////////////////////////////////////////////////

	codePipeline := &gocf.CodePipelinePipeline{
		RoleArn: gocf.GetAtt(codePipelineRoleResource, "Arn"),
		ArtifactStore: &gocf.CodePipelinePipelineArtifactStore{
			Type:     gocf.String("S3"),
			Location: gocf.Ref(artifactS3BucketResource).String(),
		},
		Stages: &gocf.CodePipelinePipelineStageDeclarationList{
			//////////////////////////////////////////////////////////////////////////
			// Source Stage
			gocf.CodePipelinePipelineStageDeclaration{
				Name: gocf.String("Source"),
				Actions: &gocf.CodePipelinePipelineActionDeclarationList{
					gocf.CodePipelinePipelineActionDeclaration{
						Name: gocf.String("GitHub"),
						ActionTypeID: &gocf.CodePipelinePipelineActionTypeID{
							Category: gocf.String("Source"),
							Owner:    gocf.String("ThirdParty"),
							Version:  gocf.String("1"),
							Provider: gocf.String("GitHub"),
						},
						Configuration: sparta.ArbitraryJSONObject{
							"Owner":                ghOwner,
							"Repo":                 ghRepo,
							"Branch":               ghBranch,
							"PollForSourceChanges": "true",
							"OAuthToken":           provisionOptions.GithubOAuthToken,
						},
						OutputArtifacts: &gocf.CodePipelinePipelineOutputArtifactList{
							gocf.CodePipelinePipelineOutputArtifact{
								Name: gocf.String("Source"),
							},
						},
						RunOrder: gocf.Integer(1),
					},
				},
			},
			//////////////////////////////////////////////////////////////////////////
			// Build Stage
			gocf.CodePipelinePipelineStageDeclaration{
				Name: gocf.String("Build"),
				Actions: &gocf.CodePipelinePipelineActionDeclarationList{
					gocf.CodePipelinePipelineActionDeclaration{
						Name: gocf.String("Build"),
						InputArtifacts: &gocf.CodePipelinePipelineInputArtifactList{
							gocf.CodePipelinePipelineInputArtifact{
								Name: gocf.String("Source"),
							},
						},
						ActionTypeID: &gocf.CodePipelinePipelineActionTypeID{
							Category: gocf.String("Build"),
							Owner:    gocf.String("AWS"),
							Version:  gocf.String("1"),
							Provider: gocf.String("CodeBuild"),
						},
						Configuration: sparta.ArbitraryJSONObject{
							"ProjectName": gocf.Ref(codeBuildProjectResource).String(),
						},
						OutputArtifacts: &gocf.CodePipelinePipelineOutputArtifactList{
							gocf.CodePipelinePipelineOutputArtifact{
								Name: gocf.String("Template"),
							},
						},
					},
				},
			},
			//////////////////////////////////////////////////////////////////////////
			// Test Stage
			gocf.CodePipelinePipelineStageDeclaration{
				Name: gocf.String("TestStage"),
				Actions: &gocf.CodePipelinePipelineActionDeclarationList{
					gocf.CodePipelinePipelineActionDeclaration{
						Name: gocf.String("CreateStack"),
						InputArtifacts: &gocf.CodePipelinePipelineInputArtifactList{
							gocf.CodePipelinePipelineInputArtifact{
								Name: gocf.String("Template"),
							},
						},
						ActionTypeID: &gocf.CodePipelinePipelineActionTypeID{
							Category: gocf.String("Deploy"),
							Owner:    gocf.String("AWS"),
							Version:  gocf.String("1"),
							Provider: gocf.String("CloudFormation"),
						},
						Configuration: sparta.ArbitraryJSONObject{
							"ActionMode":   "CREATE_UPDATE",
							"Capabilities": "CAPABILITY_IAM",
							"RoleArn":      gocf.GetAtt(cfnRoleResource, "Arn").String(),
							"StackName":    gocf.Ref("TestStackName").String(),
							"TemplateConfiguration": gocf.Join("",
								gocf.String("Template::"),
								gocf.Ref("TestStackConfig").String()),
							"TemplatePath": gocf.Join("",
								gocf.String("Template::"),
								gocf.Ref("TemplateFileName").String()),
						},
						RunOrder: gocf.Integer(1),
					},
					gocf.CodePipelinePipelineActionDeclaration{
						Name: gocf.String("ApproveTestStack"),
						ActionTypeID: &gocf.CodePipelinePipelineActionTypeID{
							Category: gocf.String("Approval"),
							Owner:    gocf.String("AWS"),
							Version:  gocf.String("1"),
							Provider: gocf.String("Manual"),
						},
						Configuration: sparta.ArbitraryJSONObject{
							"CustomData": "Would you like to create a change set to update the production stack",
						},
						RunOrder: gocf.Integer(2),
					},
				},
			},
			//////////////////////////////////////////////////////////////////////////
			// Production Stage
			gocf.CodePipelinePipelineStageDeclaration{
				Name: gocf.String("ProdStage"),
				Actions: &gocf.CodePipelinePipelineActionDeclarationList{
					// Create the production changeset
					gocf.CodePipelinePipelineActionDeclaration{
						Name: gocf.String("CreateChangeSet"),
						InputArtifacts: &gocf.CodePipelinePipelineInputArtifactList{
							gocf.CodePipelinePipelineInputArtifact{
								Name: gocf.String("Template"),
							},
						},
						ActionTypeID: &gocf.CodePipelinePipelineActionTypeID{
							Category: gocf.String("Deploy"),
							Owner:    gocf.String("AWS"),
							Version:  gocf.String("1"),
							Provider: gocf.String("CloudFormation"),
						},
						Configuration: sparta.ArbitraryJSONObject{
							"ActionMode":    "CHANGE_SET_REPLACE",
							"Capabilities":  "CAPABILITY_IAM",
							"RoleArn":       gocf.GetAtt(cfnRoleResource, "Arn").String(),
							"StackName":     gocf.Ref("ProdStackName").String(),
							"ChangeSetName": productionChangeSetName,
							"TemplateConfiguration": gocf.Join("",
								gocf.String("Template::"),
								gocf.Ref("ProdStackConfig").String()),
							"TemplatePath": gocf.Join("",
								gocf.String("Template::"),
								gocf.Ref("TemplateFileName").String()),
						},
						RunOrder: gocf.Integer(1),
					},
					// Confirm the promotion
					gocf.CodePipelinePipelineActionDeclaration{
						Name: gocf.String("ApproveChangeSet"),
						ActionTypeID: &gocf.CodePipelinePipelineActionTypeID{
							Category: gocf.String("Approval"),
							Owner:    gocf.String("AWS"),
							Version:  gocf.String("1"),
							Provider: gocf.String("Manual"),
						},
						Configuration: sparta.ArbitraryJSONObject{
							"CustomData": "Would you like to make these production changes?",
						},
						RunOrder: gocf.Integer(2),
					},
					// Confirm the promotion
					gocf.CodePipelinePipelineActionDeclaration{
						Name: gocf.String("ExecuteChangeSet"),
						ActionTypeID: &gocf.CodePipelinePipelineActionTypeID{
							Category: gocf.String("Deploy"),
							Owner:    gocf.String("AWS"),
							Version:  gocf.String("1"),
							Provider: gocf.String("CloudFormation"),
						},
						Configuration: sparta.ArbitraryJSONObject{
							"ActionMode":    "CHANGE_SET_EXECUTE",
							"ChangeSetName": productionChangeSetName,
							"RoleArn":       gocf.GetAtt(cfnRoleResource, "Arn").String(),
							"StackName":     gocf.Ref("ProdStackName").String(),
						},
						RunOrder: gocf.Integer(3),
					},
				},
			},
		},
	}
	cfTemplate.AddResource("BuildPipeline", codePipeline)

	// Save the template, post it to S3, wait for things to finish...
	scratchJSON := filepath.Join("./.sparta", "pipeline.json")
	tempFile, tempFileErr := os.Create(scratchJSON)
	if nil != tempFileErr {
		tempFile.Close()
		return tempFileErr
	}

	jsonBytes, jsonBytesErr := json.MarshalIndent(cfTemplate, "", " ")
	if nil != jsonBytesErr {
		return jsonBytesErr
	}
	_, writeErr := tempFile.Write(jsonBytes)
	if nil != writeErr {
		return writeErr
	}
	tempFile.Close()

	// Tell the user what to put as the `buildspec.yml` in the root project directory
	if provisionOptions.Noop {
		// Just log it...
		logger.WithFields(logrus.Fields{
			"TemplatePath": tempFile.Name(),
		}).Info("Bypassing upload due to --noop flag")
	} else {
		uploadLocation, uploadURLErr := spartaS3.UploadLocalFileToS3(tempFile.Name(),
			awsSession,
			provisionOptions.S3Bucket,
			fmt.Sprintf("%s-codepipelineTemplate.json", sparta.OptionsGlobal.ServiceName),
			logger)
		if nil != uploadURLErr {
			return uploadURLErr
		}
		pipelineStackName := fmt.Sprintf("%s-%s",
			sparta.OptionsGlobal.ServiceName,
			provisionOptions.PipelineName)

		stackResult, stackResultErr := spartaCF.ConvergeStackState(pipelineStackName,
			cfTemplate,
			uploadLocation,
			nil,
			time.Now(),
			awsSession,
			logger)
		if nil != stackResultErr {
			return stackResultErr
		}
		logger.WithFields(logrus.Fields{
			"StackId": *stackResult.StackId,
		}).Info("Pipeline provisioned")
	}
	// Great we have a pipeline!
	return nil
}
