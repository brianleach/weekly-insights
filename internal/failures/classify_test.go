package failures

import "testing"

func TestClassifyToolUseErrorWithoutHarnessMarkerFallsThrough(t *testing.T) {
	class, detail := Classify("Read", "", "<tool_use_error>File does not exist.</tool_use_error>")
	if class != ClassOther {
		t.Errorf("class = %q, want %q", class, ClassOther)
	}
	if detail != "" {
		t.Errorf("detail = %q, want empty", detail)
	}

	class, detail = Classify("Bash", "git status", "<tool_use_error>exit code 1</tool_use_error>")
	if class != ClassShellError {
		t.Errorf("class = %q, want %q", class, ClassShellError)
	}
	if detail != "git" {
		t.Errorf("detail = %q, want %q", detail, "git")
	}
}

func TestClassifyClassifierDenialWithoutReason(t *testing.T) {
	class, detail := Classify("Bash", "curl example.com", "Permission denied by the auto mode classifier [policy]")
	if class != ClassClassifierDenied {
		t.Errorf("class = %q, want %q", class, ClassClassifierDenied)
	}
	if detail != "" {
		t.Errorf("detail = %q, want empty", detail)
	}
}

func TestClassifyClassifierDeniedWithUnterminatedReason(t *testing.T) {
	class, detail := Classify("Bash", "env", "Denied by auto mode classifier. Reason: [Credential Leakage")
	if class != ClassClassifierDenied {
		t.Errorf("class = %q, want %q", class, ClassClassifierDenied)
	}
	if detail != "" {
		t.Errorf("detail = %q, want empty", detail)
	}
}

func TestLeadingCommandOnlyEnvAssignmentsIsEmpty(t *testing.T) {
	if got := LeadingCommand("FOO=1 BAR=2; git status"); got != "" {
		t.Errorf("LeadingCommand = %q, want empty", got)
	}
}
