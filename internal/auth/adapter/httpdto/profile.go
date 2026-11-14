// Package httpdto maps auth application data to HTTP response values.
package httpdto

import "github.com/qldapm-hcmut/ticket-system-uni/internal/auth/application"

// Profile is the user representation shared by auth and admin HTTP responses.
func Profile(p application.Profile) map[string]any {
	roles := []string{}
	if p.StudentCode != "" {
		roles = append(roles, "STUDENT")
	}
	if len(p.Memberships) > 0 {
		roles = append(roles, "ORGANIZER")
	}
	if p.IsAdmin {
		roles = append(roles, "ADMIN")
	}
	var studentCode any
	if p.StudentCode != "" {
		studentCode = p.StudentCode
	}
	units := []any{}
	for _, m := range p.Memberships {
		units = append(units, map[string]any{
			"unit": map[string]string{
				"id":   m.UnitID,
				"code": m.Code,
				"name": m.Name,
			},
			"memberRole": m.MemberRole,
		})
	}
	return map[string]any{
		"id":          p.ID,
		"email":       p.Email,
		"fullName":    p.FullName,
		"studentCode": studentCode,
		"isAdmin":     p.IsAdmin,
		"isActive":    p.IsActive,
		"roles":       roles,
		"units":       units,
	}
}
