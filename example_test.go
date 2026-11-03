package fbhook_test

import (
	"fmt"
	"github.com/darron/fbhook"
)

func ExamplePrepare() {
	feedback := fbhook.Prepare(fbhook.Feedback{Source: fbhook.Source{App: "archive"}}, fbhook.Feedback{Feedback: "Keep my search filters"})
	fmt.Println(feedback.Source.App, feedback.Feedback)
	// Output: archive Keep my search filters
}
