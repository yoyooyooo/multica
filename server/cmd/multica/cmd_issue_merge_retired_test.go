package main

import "testing"

func TestIssueMergeCommandIsRetired(t *testing.T) {
	for _, command := range issueCmd.Commands() {
		if command.Name() == "merge" {
			t.Fatal("retired issue merge command is still registered")
		}
	}
}
