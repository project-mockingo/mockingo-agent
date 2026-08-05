package naming

import "testing"

func TestValidate(t *testing.T) {
	t.Parallel()
	valid := []string{"demo", "my-spring-app", "wso2-660", "abc", "a1b"}
	invalid := []string{"", "ab", "Demo", "-my-app", "my-app-", "my_app", "my--app", "abcdefghijklmnopqrstuvwxyz123456789012345"}
	for _, name := range valid {
		if err := Validate(name); err != nil {
			t.Errorf("Validate(%q) returned %v", name, err)
		}
	}
	for _, name := range invalid {
		if err := Validate(name); err == nil {
			t.Errorf("Validate(%q) unexpectedly succeeded", name)
		}
	}
}
