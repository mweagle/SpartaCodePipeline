# SpartaCodePipeline
Sparta-based application that integrates with [CodePipeline](https://aws.amazon.com/codepipeline/).

Usage:
  1. `go get -u -v ./...`
  1. Use the AWS console to create a pipeline from _pipeline.yml_
      - Take note of the **SourceS3Key** and **S3Bucket** Parameter values
  1. Build and post the application:
      ```
      go run main.go provision --level info --s3Bucket {S3Bucket} --codePipeline {SourceS3Key}
      ````

For more information, see the AWS [blog post](https://aws.amazon.com/blogs/aws/codepipeline-update-build-continuous-delivery-workflows-for-cloudformation-stacks/).
