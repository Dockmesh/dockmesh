package audit

import "sort"

// Audit categories group actions for the audit page and its filter.
const (
	CategoryChanges = "changes" // created, changed, deployed or removed something
	CategoryAccess  = "access"  // read data: file browsing and reads, exec, downloads
	CategoryAuth    = "auth"    // sign-in, sessions, tokens, MFA, SSO, scope denials
	CategorySystem  = "system"  // the server itself and the audit trail
)

// Categories lists the valid categories in display order.
var Categories = []string{CategoryChanges, CategoryAccess, CategoryAuth, CategorySystem}

// categories maps every action the code emits to its category.
// TestEveryEmittedActionHasCategory fails when an emitted action is
// missing here.
var categories = map[string]string{
	ActionLogin:                 CategoryAuth,
	ActionLoginFailed:           CategoryAuth,
	ActionLoginBlocked:          CategoryAuth,
	ActionLogout:                CategoryAuth,
	ActionRefresh:               CategoryAuth,
	ActionSAMLRekey:             CategoryAuth,
	ActionSAMLRekeyEnd:          CategoryAuth,
	ActionScopeDenied:           CategoryAuth,
	"auth.sso_login":            CategoryAuth,
	"auth.session_revoke":       CategoryAuth,
	"auth.session_revoke_all":   CategoryAuth,
	"auth.policy_update":        CategoryAuth,
	"auth.signin_config_update": CategoryAuth,
	"api_token.create":          CategoryAuth,
	"api_token.revoke":          CategoryAuth,
	ActionUserPassword:          CategoryAuth,
	"user.unlock":               CategoryAuth,
	"user.mfa_enroll":           CategoryAuth,
	"user.mfa_disable":          CategoryAuth,
	"user.mfa_disable_failed":   CategoryAuth,
	"user.mfa_reset":            CategoryAuth,
	"oidc.provider_create":      CategoryAuth,
	"oidc.provider_update":      CategoryAuth,
	"oidc.provider_delete":      CategoryAuth,
	"oidc.reload":               CategoryAuth,
	"saml.provider_create":      CategoryAuth,
	"saml.provider_update":      CategoryAuth,
	"saml.provider_delete":      CategoryAuth,
	"ldap.provider_create":      CategoryAuth,
	"ldap.provider_update":      CategoryAuth,
	"ldap.provider_delete":      CategoryAuth,
	"oauth2.provider_create":    CategoryAuth,
	"oauth2.provider_update":    CategoryAuth,
	"oauth2.provider_delete":    CategoryAuth,

	ActionContainerBrowse:    CategoryAccess,
	ActionContainerReadFile:  CategoryAccess,
	ActionContainerExecStart: CategoryAccess,
	ActionContainerExecEnd:   CategoryAccess,
	ActionVolumeBrowse:       CategoryAccess,
	ActionVolumeReadFile:     CategoryAccess,
	ActionBackupDownload:     CategoryAccess,
	ActionBackupKeyExport:    CategoryAccess,
	"backup.verify_run":      CategoryAccess,
	"backup.verify_upload":   CategoryAccess,

	ActionUserCreate:                CategoryChanges,
	ActionUserUpdate:                CategoryChanges,
	ActionUserDelete:                CategoryChanges,
	"user.avatar_update":            CategoryChanges,
	"user.avatar_delete":            CategoryChanges,
	"user.invite_created":           CategoryChanges,
	"user.invite_revoked":           CategoryChanges,
	ActionRoleCreate:                CategoryChanges,
	ActionRoleUpdate:                CategoryChanges,
	ActionRoleDelete:                CategoryChanges,
	ActionStackCreate:               CategoryChanges,
	ActionStackUpdate:               CategoryChanges,
	ActionStackDelete:               CategoryChanges,
	ActionStackDeploy:               CategoryChanges,
	ActionStackStop:                 CategoryChanges,
	ActionStackAdopt:                CategoryChanges,
	"stack.accept_external_changes": CategoryChanges,
	"stack.git_configure":           CategoryChanges,
	"stack.git_disconnect":          CategoryChanges,
	"stack.git_sync":                CategoryChanges,
	"stack.git_webhook":             CategoryChanges,
	ActionContainerStart:            CategoryChanges,
	ActionContainerStop:             CategoryChanges,
	ActionContainerKill:             CategoryChanges,
	"container.kill":                CategoryChanges,
	"container.pause":               CategoryChanges,
	"container.unpause":             CategoryChanges,
	ActionContainerRm:               CategoryChanges,
	ActionContainerUpdate:           CategoryChanges,
	ActionContainerRollback:         CategoryChanges,
	ActionContainerWriteFile:        CategoryChanges,
	ActionImagePull:                 CategoryChanges,
	ActionImageRemove:               CategoryChanges,
	ActionImagePrune:                CategoryChanges,
	ActionImageScan:                 CategoryChanges,
	ActionNetworkCreate:             CategoryChanges,
	ActionNetworkRemove:             CategoryChanges,
	ActionVolumeCreate:              CategoryChanges,
	ActionVolumeRemove:              CategoryChanges,
	ActionVolumePrune:               CategoryChanges,
	"template.create":               CategoryChanges,
	"template.update":               CategoryChanges,
	"template.delete":               CategoryChanges,
	"template.deploy":               CategoryChanges,
	"registry.create":               CategoryChanges,
	"registry.update":               CategoryChanges,
	"registry.delete":               CategoryChanges,
	"registry.test":                 CategoryChanges,
	"host.tags_set":                 CategoryChanges,
	"host.tag_add":                  CategoryChanges,
	"host.tag_remove":               CategoryChanges,
	"agent.rotate_token":            CategoryChanges,
	"agent.upgrade_policy":          CategoryChanges,
	"agent.upgrade_run":             CategoryChanges,
	"alerts.rule_create":            CategoryChanges,
	"alerts.rule_update":            CategoryChanges,
	"alerts.rule_delete":            CategoryChanges,
	"notify.channel_create":         CategoryChanges,
	"notify.channel_update":         CategoryChanges,
	"notify.channel_delete":         CategoryChanges,
	"proxy.enable":                  CategoryChanges,
	"proxy.disable":                 CategoryChanges,
	"proxy.route_create":            CategoryChanges,
	"proxy.route_update":            CategoryChanges,
	"proxy.route_enabled":           CategoryChanges,
	"proxy.route_disabled":          CategoryChanges,
	"proxy.route_delete":            CategoryChanges,
	"backup.accept_host_key":        CategoryChanges,
	"backup.accept_target_host_key": CategoryChanges,

	ActionGenesis:               CategorySystem,
	ActionChainBridge:           CategorySystem,
	ActionBackupJobRepaired:     CategorySystem,
	"audit.retention_configure": CategorySystem,
	"audit.retention_run":       CategorySystem,
	"audit.webhook_configure":   CategorySystem,
	"audit.webhook_test":        CategorySystem,
	"secrets.rotate":            CategorySystem,
	"system.update_check":       CategorySystem,
}

// Category returns the category of an action. Actions without an entry,
// such as ones a newer or older version wrote, count as system.
func Category(action string) string {
	if c, ok := categories[action]; ok {
		return c
	}
	return CategorySystem
}

// ValidCategory reports whether c is one of Categories.
func ValidCategory(c string) bool {
	for _, v := range Categories {
		if v == c {
			return true
		}
	}
	return false
}

// actionsIn returns the known actions of category c, sorted.
func actionsIn(c string) []string {
	var out []string
	for a, cat := range categories {
		if cat == c {
			out = append(out, a)
		}
	}
	sort.Strings(out)
	return out
}
