# SpartaCodePipeline
Sparta-based application that integrates with [CodePipeline](https://aws.amazon.com/codepipeline/).

See the [Medium Post](https://medium.com/@mweagle/serverless-serverfull-and-weaving-pipelines-c9f83eec9227) for more information.


## Quick Summary

1. Provision the pipeline:

```
go run main.go provisionPipeline --pipeline MySpartaPipelineName --repo https://github.com/mweagle/SpartaCodePipeline --oauth $GITHUB_AUTH_TOKEN --s3Bucket $MY_S3_BUCKET
```

2. Visit the AWS console & manage the build, approval workflow