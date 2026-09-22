package main

import "testing"

// A key that names a password, a token, a key or a secret is masked by default:
// an agent reading a workload's environment must not be handed credentials it
// did not ask to see. Ordinary configuration comes through.
func TestSecretLookingKeysAreMaskedByDefault(t *testing.T) {
	for _, k := range []string{"NEO_ODB_PASSWORD", "AMQP_PASSWORD", "MONGODB_PWD", "GITHUB_TOKEN", "API_KEY", "AWS_SECRET_ACCESS_KEY", "OAUTH_CLIENT_SECRET", "BASIC_AUTH"} {
		if !looksSecret(k) {
			t.Errorf("%s should be masked", k)
		}
	}
	for _, k := range []string{"NEO_ODB_HOST", "PORT", "LOG_LEVEL", "FPL_DISPLAY_TIMEZONE", "NEO_MONGODB_LOGIN"} {
		if looksSecret(k) {
			t.Errorf("%s is plain configuration and must come through", k)
		}
	}
}

func TestStatusWordsAreTheThreeAnAgentBranchesOn(t *testing.T) {
	if statusWord(stOK) != "ok" || statusWord(stWarn) != "warn" || statusWord(stFail) != "fail" {
		t.Fatal("status words drifted")
	}
}
