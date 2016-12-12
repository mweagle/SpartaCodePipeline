package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/Sirupsen/logrus"
	sparta "github.com/mweagle/Sparta"
)

func init() {
	sparta.RegisterCodePipelineEnvironment("test", map[string]string{
		"MESSAGE": "Hello Test!",
	})
	sparta.RegisterCodePipelineEnvironment("production", map[string]string{
		"MESSAGE": "Hello Production!",
	})
}

// Standard AWS λ function
func helloWorld(event *json.RawMessage,
	context *sparta.LambdaContext,
	w http.ResponseWriter,
	logger *logrus.Logger) {

	messageText := os.Getenv("MESSAGE")
	if "" == messageText {
		messageText = "$MESSAGE not defined"
	}
	fmt.Fprint(w, messageText)
}

////////////////////////////////////////////////////////////////////////////////
// Main
func main() {

	lambdaFn := sparta.NewLambda(sparta.IAMRoleDefinition{},
		helloWorld,
		nil)

	var lambdaFunctions []*sparta.LambdaAWSInfo
	lambdaFunctions = append(lambdaFunctions, lambdaFn)
	err := sparta.Main("SpartaCodePipeline",
		fmt.Sprintf("Test CodePipeline deployment"),
		lambdaFunctions,
		nil,
		nil)
	if err != nil {
		os.Exit(1)
	}
}
