package gitsafe

import "testing"

func TestValidArg(t *testing.T) {
	valid := []string{
		"owner/repo",
		"https://github.com/owner/repo.git",
		"git@github.com:owner/repo.git",
		"/abs/local/path",
		"develop",
		"main",
		"feature/x-1",
	}
	for _, s := range valid {
		if !ValidArg(s) {
			t.Errorf("ValidArg(%q) = false, want true", s)
		}
	}

	invalid := []string{
		"",                 // empty
		"-x",               // leading dash → option
		"--upload-pack=/x", // the RCE vector
		"--output=/etc/x",  //
		"-",                //
		"a b",              // embedded space
		"a\tb",             // tab
		"a\nb",             // newline
		"a\rb",             // CR
		"a\x00b",           // NUL / control
		"x\x7f",            // DEL
	}
	for _, s := range invalid {
		if ValidArg(s) {
			t.Errorf("ValidArg(%q) = true, want false", s)
		}
	}
}
