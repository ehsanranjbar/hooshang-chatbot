// Package auth provides admin checks for hooshang.
package auth

// Admins decides whether a user may administer a chat. All admin checks go
// through this interface so other sources of admins can be added later.
type Admins interface {
	IsAdmin(chatID, userID int64) bool
}

// List is the Admins implementation backed by telegram.admin_ids: a fixed set
// of users who are admins in every chat.
type List struct{ IDs []int64 }

// IsAdmin reports whether userID is one of the listed admins.
func (l List) IsAdmin(_, userID int64) bool {
	for _, id := range l.IDs {
		if id == userID {
			return true
		}
	}
	return false
}
