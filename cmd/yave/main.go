package main

import (
	"fmt"
	"os"

	"fyne.io/fyne/v2/app"

	"git.skobk.in/skobkin/yave/internal/ui"
)

func main() {
	a := app.NewWithID("in.skobk.yave")
	initialInput := ""
	if len(os.Args) > 1 {
		initialInput = os.Args[1]
	}
	controller, err := ui.New(a, initialInput)
	if err != nil {
		fmt.Fprintln(os.Stderr, "YAVE:", err)
		os.Exit(1)
	}
	controller.ShowAndRun()
}
