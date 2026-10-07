package targets

import "testing"

func TestCredentialSetForResolvesRoles(t *testing.T) {
	backup := PlainCreds{AccessKeyID: "AKIA-BACKUP", SecretAccessKey: "backup"}
	maint := PlainCreds{AccessKeyID: "AKIA-MAINT", SecretAccessKey: "maintenance"}

	cases := []struct {
		name            string
		set             CredentialSet
		wantBackup      PlainCreds
		wantMaintenance PlainCreds
		wantSeparated   bool
	}{
		{
			name:            "separated",
			set:             CredentialSet{Backup: backup, Maintenance: maint},
			wantBackup:      backup,
			wantMaintenance: maint,
			wantSeparated:   true,
		},
		{
			// The single-key deployment. Both roles resolve to the one key, and
			// it does not claim to be separated.
			name:            "one key",
			set:             CredentialSet{Backup: backup},
			wantBackup:      backup,
			wantMaintenance: backup,
		},
		{
			// An operator who points both roles at the same key gets the same
			// behaviour as configuring one. Reporting it as separated would put
			// a false statement in the runbook's diagnostics.
			name:            "same key twice",
			set:             CredentialSet{Backup: backup, Maintenance: backup},
			wantBackup:      backup,
			wantMaintenance: backup,
		},
		{
			// Only reachable from a record written by something that bypassed
			// Seal, which refuses this. The fallback is still the safe answer:
			// a run with an unusable credential is worse than a run with the
			// one credential known to work.
			name:            "half-filled maintenance falls back",
			set:             CredentialSet{Backup: backup, Maintenance: PlainCreds{AccessKeyID: "x"}},
			wantBackup:      backup,
			wantMaintenance: backup,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.set.For(RoleBackup); got != tc.wantBackup {
				t.Errorf("For(RoleBackup) = %+v, want %+v", got, tc.wantBackup)
			}
			if got := tc.set.For(RoleMaintenance); got != tc.wantMaintenance {
				t.Errorf("For(RoleMaintenance) = %+v, want %+v", got, tc.wantMaintenance)
			}
			if got := tc.set.Separated(); got != tc.wantSeparated {
				t.Errorf("Separated() = %v, want %v", got, tc.wantSeparated)
			}
		})
	}
}

func TestPlainCredsCompleteness(t *testing.T) {
	cases := []struct {
		name                    string
		creds                   PlainCreds
		wantComplete, wantEmpty bool
	}{
		{"both", PlainCreds{AccessKeyID: "a", SecretAccessKey: "b"}, true, false},
		{"neither", PlainCreds{}, false, true},
		{"id only", PlainCreds{AccessKeyID: "a"}, false, false},
		{"secret only", PlainCreds{SecretAccessKey: "b"}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.creds.Complete(); got != tc.wantComplete {
				t.Errorf("Complete() = %v, want %v", got, tc.wantComplete)
			}
			if got := tc.creds.Empty(); got != tc.wantEmpty {
				t.Errorf("Empty() = %v, want %v", got, tc.wantEmpty)
			}
		})
	}
}
