package workiq

// AccountFact is archived identity evidence, not a new account registration.
type AccountFact struct {
	ID                string   `json:"account_id"`
	Family            string   `json:"family"`
	UserID            string   `json:"-"`
	TenantID          string   `json:"-"`
	Addresses         []string `json:"-"`
	IdentityPresent   bool     `json:"-"`
	MultipleMailboxes bool     `json:"-"`
}
