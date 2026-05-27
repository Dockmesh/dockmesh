// Shared types for the authentication page and its private components.
// Slice 1: only OIDC has a real backend; OAuth2/SAML/LDAP forms render
// fully but their save buttons are disabled (Marcel feedback 2026-05-09 —
// no scaffolding hints, just disabled state).

export type ProviderKind = 'oidc' | 'oauth2' | 'saml' | 'ldap';

export interface GroupMapping {
  group: string;
  role: string;
}

export interface ProviderBase {
  slug: string;
  display_name: string;
  enabled: boolean;
  default_role: string;
  group_mappings: GroupMapping[];
}

export interface OIDCConfig extends ProviderBase {
  kind: 'oidc';
  issuer_url: string;
  client_id: string;
  client_secret: string;
  scopes: string;
  group_claim: string;
}

export interface OAuth2Config extends ProviderBase {
  kind: 'oauth2';
  authorization_url: string;
  token_url: string;
  userinfo_url: string;
  client_id: string;
  client_secret: string;
  scopes: string;
  username_field: string;
  email_field: string;
  groups_field: string;
}

export interface SAMLConfig extends ProviderBase {
  kind: 'saml';
  idp_metadata_url: string;
  idp_metadata_xml: string;
  sp_entity_id: string;
  signing_cert: string;
  encryption_cert: string;
  nameid_format: string;
  username_attribute: string;
  email_attribute: string;
  groups_attribute: string;
}

export interface LDAPConfig extends ProviderBase {
  kind: 'ldap';
  mode: 'openldap' | 'ad';
  host: string;
  port: number;
  tls: 'ldaps' | 'starttls' | 'none';
  skip_verify: boolean;
  bind_dn: string;
  bind_password: string;
  user_search_base: string;
  user_search_filter: string;
  username_attribute: string;
  email_attribute: string;
  group_search_base: string;
  group_membership_attribute: string;
}

export type ProviderConfig = OIDCConfig | OAuth2Config | SAMLConfig | LDAPConfig;

// ── Templates ────────────────────────────────────────────────────────
// Each template prefills the form for a popular IdP. Generic = blank.

export interface OIDCTemplate {
  id: string;
  label: string;
  icon: string;
  issuer_hint: string;
  scopes: string;
  help: string;
}

export const OIDC_TEMPLATES: OIDCTemplate[] = [
  { id: 'keycloak', label: 'Keycloak', icon: '🔐',
    issuer_hint: 'https://auth.example/realms/main',
    scopes: 'openid profile email groups',
    help: 'Self-hosted realm. Map realm-roles or groups to Dockmesh roles.' },
  { id: 'authelia', label: 'Authelia', icon: '🛡️',
    issuer_hint: 'https://auth.example',
    scopes: 'openid profile email groups',
    help: 'Self-hosted, lighter than Keycloak. groups claim available since 4.x.' },
  { id: 'azure', label: 'Microsoft 365 (Azure AD)', icon: '🪟',
    issuer_hint: 'https://login.microsoftonline.com/{tenant-id}/v2.0',
    scopes: 'openid profile email',
    help: 'Multi-tenant: tenant-id = "common". Single-tenant: paste your directory ID.' },
  { id: 'google', label: 'Google', icon: 'G',
    issuer_hint: 'https://accounts.google.com',
    scopes: 'openid profile email',
    help: 'Restrict to a Workspace domain via "hd" claim if needed.' },
  { id: 'gitlab', label: 'GitLab', icon: '🦊',
    issuer_hint: 'https://gitlab.example',
    scopes: 'openid profile email',
    help: 'Self-hosted GitLab works the same as gitlab.com.' },
  { id: 'generic', label: 'Generic OIDC', icon: '○',
    issuer_hint: 'https://auth.example',
    scopes: 'openid profile email',
    help: 'Anything that speaks the OpenID-Connect spec.' },
];

export interface OAuth2Template {
  id: string;
  label: string;
  icon: string;
  authorization_url: string;
  token_url: string;
  userinfo_url: string;
  scopes: string;
  username_field: string;
  email_field: string;
  groups_field: string;
  help: string;
}

export const OAUTH2_TEMPLATES: OAuth2Template[] = [
  { id: 'github', label: 'GitHub', icon: '🐙',
    authorization_url: 'https://github.com/login/oauth/authorize',
    token_url: 'https://github.com/login/oauth/access_token',
    userinfo_url: 'https://api.github.com/user',
    scopes: 'read:user user:email read:org',
    username_field: 'login',
    email_field: 'email',
    groups_field: '',
    help: 'OAuth Apps under Developer Settings. Org-level groups need read:org and a separate /user/orgs lookup.' },
  { id: 'gitlab', label: 'GitLab OAuth', icon: '🦊',
    authorization_url: 'https://gitlab.com/oauth/authorize',
    token_url: 'https://gitlab.com/oauth/token',
    userinfo_url: 'https://gitlab.com/api/v4/user',
    scopes: 'read_user',
    username_field: 'username',
    email_field: 'email',
    groups_field: '',
    help: 'OAuth-only path (no OIDC). Prefer OIDC unless you need fine-grained scopes.' },
  { id: 'google', label: 'Google OAuth', icon: 'G',
    authorization_url: 'https://accounts.google.com/o/oauth2/v2/auth',
    token_url: 'https://oauth2.googleapis.com/token',
    userinfo_url: 'https://www.googleapis.com/oauth2/v3/userinfo',
    scopes: 'openid email profile',
    username_field: 'email',
    email_field: 'email',
    groups_field: '',
    help: 'OAuth-only path. OIDC is recommended for Google.' },
  { id: 'generic', label: 'Generic OAuth2', icon: '○',
    authorization_url: 'https://auth.example/oauth/authorize',
    token_url: 'https://auth.example/oauth/token',
    userinfo_url: 'https://auth.example/oauth/userinfo',
    scopes: '',
    username_field: 'username',
    email_field: 'email',
    groups_field: 'groups',
    help: 'Any RFC 6749 OAuth 2.0 server with a userinfo-style endpoint.' },
];

export interface SAMLTemplate {
  id: string;
  label: string;
  icon: string;
  idp_metadata_url_hint: string;
  username_attribute: string;
  email_attribute: string;
  groups_attribute: string;
  nameid_format: string;
  help: string;
}

export const SAML_TEMPLATES: SAMLTemplate[] = [
  { id: 'azure', label: 'Azure AD (Microsoft Entra)', icon: '🪟',
    idp_metadata_url_hint: 'https://login.microsoftonline.com/{tenant-id}/federationmetadata/2007-06/federationmetadata.xml',
    username_attribute: 'http://schemas.xmlsoap.org/ws/2005/05/identity/claims/name',
    email_attribute: 'http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress',
    groups_attribute: 'http://schemas.microsoft.com/ws/2008/06/identity/claims/groups',
    nameid_format: 'urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress',
    help: 'Configure as Enterprise App. Add SP entity ID + ACS URL in App Registration.' },
  { id: 'adfs', label: 'AD FS', icon: '🪟',
    idp_metadata_url_hint: 'https://adfs.example.com/FederationMetadata/2007-06/FederationMetadata.xml',
    username_attribute: 'http://schemas.xmlsoap.org/ws/2005/05/identity/claims/upn',
    email_attribute: 'http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress',
    groups_attribute: 'http://schemas.xmlsoap.org/claims/Group',
    nameid_format: 'urn:oasis:names:tc:SAML:1.1:nameid-format:unspecified',
    help: 'Add Relying Party Trust on the AD FS server with the ACS URL below.' },
  { id: 'okta', label: 'Okta', icon: '🌀',
    idp_metadata_url_hint: 'https://your-tenant.okta.com/app/{app-id}/sso/saml/metadata',
    username_attribute: 'NameID',
    email_attribute: 'email',
    groups_attribute: 'groups',
    nameid_format: 'urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress',
    help: 'Create a new SAML 2.0 application in Okta. Configure attribute statements for groups.' },
  { id: 'authentik', label: 'Authentik / Keycloak', icon: '🔐',
    idp_metadata_url_hint: 'https://auth.example/application/saml/dockmesh/metadata/',
    username_attribute: 'http://schemas.goauthentik.io/2021/02/saml/username',
    email_attribute: 'http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress',
    groups_attribute: 'http://schemas.xmlsoap.org/claims/Group',
    nameid_format: 'urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress',
    help: 'Both Authentik and Keycloak ship SAML provider templates. Use the upstream metadata URL.' },
  { id: 'generic', label: 'Generic SAML 2.0', icon: '○',
    idp_metadata_url_hint: 'https://idp.example/metadata',
    username_attribute: 'NameID',
    email_attribute: 'email',
    groups_attribute: 'groups',
    nameid_format: 'urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress',
    help: 'Any SAML 2.0 IdP with HTTP-Redirect or HTTP-POST bindings.' },
];

// ── Helper: detect kind from issuer/host ─────────────────────────────
export function detectOIDCTemplate(issuer: string): string {
  const u = issuer.toLowerCase();
  if (/realms\//.test(u) || /keycloak/.test(u)) return 'keycloak';
  if (/authelia/.test(u)) return 'authelia';
  if (/login\.microsoftonline\.com/.test(u)) return 'azure';
  if (/accounts\.google\.com/.test(u)) return 'google';
  if (/gitlab/.test(u)) return 'gitlab';
  return 'generic';
}

// ── Default factories ────────────────────────────────────────────────
export function newOIDCConfig(): OIDCConfig {
  return {
    kind: 'oidc',
    slug: '',
    display_name: '',
    enabled: true,
    issuer_url: '',
    client_id: '',
    client_secret: '',
    scopes: 'openid profile email',
    group_claim: 'groups',
    default_role: 'viewer',
    group_mappings: [],
  };
}

export function newOAuth2Config(): OAuth2Config {
  return {
    kind: 'oauth2',
    slug: '',
    display_name: '',
    enabled: true,
    authorization_url: '',
    token_url: '',
    userinfo_url: '',
    client_id: '',
    client_secret: '',
    scopes: '',
    username_field: 'username',
    email_field: 'email',
    groups_field: 'groups',
    default_role: 'viewer',
    group_mappings: [],
  };
}

export function newSAMLConfig(): SAMLConfig {
  return {
    kind: 'saml',
    slug: '',
    display_name: '',
    enabled: true,
    idp_metadata_url: '',
    idp_metadata_xml: '',
    sp_entity_id: '',
    signing_cert: '',
    encryption_cert: '',
    nameid_format: 'urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress',
    username_attribute: 'NameID',
    email_attribute: 'email',
    groups_attribute: 'groups',
    default_role: 'viewer',
    group_mappings: [],
  };
}

export function newLDAPConfig(): LDAPConfig {
  return {
    kind: 'ldap',
    slug: '',
    display_name: '',
    enabled: true,
    mode: 'openldap',
    host: '',
    port: 636,
    tls: 'ldaps',
    skip_verify: false,
    bind_dn: '',
    bind_password: '',
    user_search_base: '',
    user_search_filter: '(&(objectClass=person)(uid=%s))',
    username_attribute: 'uid',
    email_attribute: 'mail',
    group_search_base: '',
    group_membership_attribute: 'memberOf',
    default_role: 'viewer',
    group_mappings: [],
  };
}

export function ldapDefaultsForMode(mode: 'openldap' | 'ad'): Partial<LDAPConfig> {
  if (mode === 'ad') {
    return {
      user_search_filter: '(&(objectCategory=Person)(sAMAccountName=%s))',
      username_attribute: 'sAMAccountName',
      email_attribute: 'mail',
      group_membership_attribute: 'memberOf',
    };
  }
  return {
    user_search_filter: '(&(objectClass=person)(uid=%s))',
    username_attribute: 'uid',
    email_attribute: 'mail',
    group_membership_attribute: 'memberOf',
  };
}
