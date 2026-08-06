package naming

import "fmt"

var reserved = map[string]struct{}{
	"api": {}, "www": {}, "app": {}, "gateway": {}, "admin": {}, "auth": {},
	"status": {}, "health": {}, "docs": {}, "support": {}, "mail": {}, "smtp": {},
	"ftp": {}, "localhost": {},
}

func Validate(name string) error {
	if len(name) < 3 || len(name) > 40 {
		return fmt.Errorf("tunnel name must be between 3 and 40 characters")
	}
	for i, char := range []byte(name) {
		valid := char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '-'
		if !valid {
			return fmt.Errorf("tunnel name may contain only lowercase letters, digits, and hyphens")
		}
		if char == '-' && (i == 0 || i == len(name)-1 || name[i-1] == '-') {
			return fmt.Errorf("tunnel name must not start or end with a hyphen or contain consecutive hyphens")
		}
	}
	if _, found := reserved[name]; found {
		return fmt.Errorf("endpoint name %q is reserved", name)
	}
	return nil
}
