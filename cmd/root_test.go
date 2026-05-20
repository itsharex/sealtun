package cmd

import "testing"

func TestRootSilencesCobraErrorPrinting(t *testing.T) {
	if !rootCmd.SilenceErrors {
		t.Fatal("root command should let Execute print errors once")
	}
}
