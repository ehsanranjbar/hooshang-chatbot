// Package auth provides admin checks for hooshang.
package auth

// Admins decides whether a user may administer a chat. All admin checks go
// through this interface so more admins can be added later.
type Admins interface {
	IsAdmin(chatID, userID int64) bool
}

// Owner is the only Admins implementation for now: the single hard admin
// from telegram.owner_id.
type Owner struct{ ID int64 }

// IsAdmin reports whether userID is the owner.
func (o Owner) IsAdmin(_, userID int64) bool { return userID == o.ID }
