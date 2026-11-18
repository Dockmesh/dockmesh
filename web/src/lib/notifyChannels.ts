import { m } from '$lib/paraglide/messages.js';

export interface FieldDef {
  key: string;
  label: string;
  placeholder?: string;
  secret?: boolean;
}

export interface ChannelType {
  id: string;
  label: string;
  fields: FieldDef[];
}

/** The notification channel types with the fields of their form. */
export function channelTypes(): ChannelType[] {
  return [
    { id: 'webhook',  label: m.alerts_ch_generic_webhook(),
      fields: [{ key: 'url', label: m.alerts_ch_webhook_url(), placeholder: 'https://example.com/hook' }] },
    { id: 'ntfy',     label: 'ntfy.sh',
      fields: [
        { key: 'url', label: m.alerts_ch_topic_url(), placeholder: 'https://ntfy.sh/my-topic' },
        { key: 'priority', label: m.alerts_ch_priority(), placeholder: '3' },
      ] },
    { id: 'discord',  label: 'Discord',
      fields: [{ key: 'url', label: m.alerts_ch_webhook_url(), placeholder: 'https://discord.com/api/webhooks/...' }] },
    { id: 'slack',    label: 'Slack',
      fields: [{ key: 'url', label: m.alerts_ch_webhook_url(), placeholder: 'https://hooks.slack.com/services/...' }] },
    { id: 'teams',    label: 'Microsoft Teams',
      fields: [{ key: 'url', label: m.alerts_ch_webhook_url(), placeholder: 'https://outlook.office.com/webhook/...' }] },
    { id: 'gotify',   label: 'Gotify',
      fields: [
        { key: 'url', label: m.alerts_ch_server_url(), placeholder: 'https://gotify.example.com' },
        { key: 'token', label: m.alerts_ch_app_token_gotify(), placeholder: 'APP_TOKEN', secret: true },
      ] },
    { id: 'email',    label: m.alerts_ch_email(),
      fields: [
        { key: 'host', label: m.alerts_ch_smtp_host(), placeholder: 'smtp.example.com' },
        { key: 'port', label: m.common_port(), placeholder: '587' },
        { key: 'username', label: m.alerts_ch_username() },
        { key: 'password', label: m.alerts_ch_password(), secret: true },
        { key: 'from', label: m.alerts_ch_from(), placeholder: 'alerts@example.com' },
        { key: 'to', label: m.alerts_ch_to(), placeholder: 'admin@example.com' },
      ] },
    { id: 'pagerduty', label: 'PagerDuty',
      fields: [
        { key: 'integration_key', label: m.alerts_ch_integration_key(), placeholder: 'R01A2B3C4D5...' },
        { key: 'client', label: m.alerts_ch_client_name(), placeholder: 'Dockmesh' },
        { key: 'client_url', label: m.alerts_ch_client_url(), placeholder: 'https://dockmesh.example.com' },
      ] },
    { id: 'pushover', label: 'Pushover',
      fields: [
        { key: 'app_token', label: m.alerts_ch_app_token(), placeholder: 'azGDORePK8gMaC0QOYAMyEEuzJnyUi', secret: true },
        { key: 'user_key',  label: m.alerts_ch_user_key(),  placeholder: 'uQiRzpo4DXghDmr9QzzfQu27cmVRsG', secret: true },
        { key: 'device',    label: m.alerts_ch_device(), placeholder: 'iphone' },
        { key: 'sound',     label: m.alerts_ch_sound(), placeholder: 'siren' },
      ] },
  ];
}

/** The config the API takes for the form values of type t. */
export function channelConfig(t: ChannelType, values: Record<string, string>, language: string): Record<string, unknown> {
  const config: Record<string, unknown> = {};
  for (const f of t.fields) {
    const val = values[f.key] ?? '';
    if (f.key === 'port' || f.key === 'priority') {
      config[f.key] = parseInt(val, 10) || 0;
    } else if (f.key === 'to') {
      config[f.key] = val.split(',').map((s) => s.trim()).filter(Boolean);
    } else {
      config[f.key] = val;
    }
  }
  config.language = language;
  return config;
}
