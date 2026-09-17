package httpapi

import "strings"

func (s *Server) sub2NameWithMother(name, adminID, fallbackEmail string) string {
	motherName := strings.TrimSpace(fallbackEmail)
	if strings.TrimSpace(adminID) != "" && s.store != nil {
		if admin, err := s.store.AdminAccountProfile(adminID); err == nil {
			motherName = strings.TrimSpace(admin.Label)
			if motherName == "" {
				motherName = strings.TrimSpace(admin.Email)
			}
		}
	}
	if motherName == "" || strings.HasPrefix(name, motherName+"|") {
		return name
	}
	return motherName + "|" + name
}
