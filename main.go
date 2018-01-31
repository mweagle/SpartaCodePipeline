package main

import (
	"fmt"
	"os"

	sparta "github.com/mweagle/Sparta"
	"github.com/mweagle/SpartaCodePipeline/pipeline"
	"github.com/spf13/cobra"
	"gopkg.in/go-playground/validator.v9"
)

// PipelineName is the name of the stack to provision that supports the pipeline
var pipelineOptions pipeline.ProvisionOptions

func init() {
	sparta.RegisterCodePipelineEnvironment("test", map[string]string{
		"MESSAGE":          "Hello Test!",
		"ENVIRONMENT_NAME": "test",
	})
	sparta.RegisterCodePipelineEnvironment("production", map[string]string{
		"MESSAGE":          "Hello Production!",
		"ENVIRONMENT_NAME": "prod",
	})
}

// Standard AWS Lambda function
func helloSpartaWorld() (string, error) {
	messageText := os.Getenv("MESSAGE")
	if "" == messageText {
		messageText = "$MESSAGE not defined"
	}
	return messageText, nil
}

////////////////////////////////////////////////////////////////////////////////
// Add a command to provision a CI pipeline
var pipelineProvisionCommand = &cobra.Command{
	Use:   "provisionPipeline",
	Short: "Provision a CI/CD pipeline for this stack",
	RunE: func(cmd *cobra.Command, args []string) error {
		validate := validator.New()
		cliErrors := validate.Struct(&pipelineOptions)
		if cliErrors != nil {
			return cliErrors
		}
		return pipeline.Provision(&pipelineOptions)
	},
}

////////////////////////////////////////////////////////////////////////////////
// Main
func main() {
	// Register the provisionPipeline command
	pipelineProvisionCommand.PersistentFlags().StringVarP(&pipelineOptions.PipelineName, "pipeline", "p", "", "pipeline name")
	pipelineProvisionCommand.PersistentFlags().StringVarP(&pipelineOptions.GithubRepo, "repo", "r", "", "GitHub Repo URL")
	pipelineProvisionCommand.PersistentFlags().StringVarP(&pipelineOptions.GithubOAuthToken, "oauth", "o", "", "GitHub OAuth token")
	pipelineProvisionCommand.PersistentFlags().StringVarP(&pipelineOptions.S3Bucket,
		"s3Bucket",
		"s",
		"",
		"S3 Bucket to use for Lambda source")
	pipelineProvisionCommand.PersistentFlags().BoolVarP(&pipelineOptions.Noop, "noop",
		"n",
		false,
		"Dry-run behavior only (do not perform mutations)")
	sparta.CommandLineOptions.Root.AddCommand(pipelineProvisionCommand)

	// Normal execution
	lambdaFn := sparta.HandleAWSLambda(fmt.Sprintf("HelloWorld-%s", os.Getenv("ENVIRONMENT_NAME")),
		helloSpartaWorld,
		sparta.IAMRoleDefinition{})
	var lambdaFunctions []*sparta.LambdaAWSInfo
	lambdaFunctions = append(lambdaFunctions, lambdaFn)
	err := sparta.Main("SpartaCodePipeline",
		fmt.Sprintf("SpartaCodePipeline CodePipeline example"),
		lambdaFunctions,
		nil,
		nil)

	if err != nil {
		os.Exit(1)
	}
}
