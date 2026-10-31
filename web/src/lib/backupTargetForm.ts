export type TargetType = 'local' | 'sftp' | 'smb' | 'webdav' | 's3';

export type TargetConfig = Record<string, any>;

export function connectionValid(type: TargetType, cfg: TargetConfig): boolean {
  switch (type) {
    case 'local':
      return !!cfg.path;
    case 'sftp':
      return !!cfg.host && !!cfg.username;
    case 'smb':
      return !!cfg.host && !!cfg.share;
    case 'webdav':
      return !!cfg.url && !!cfg.username;
    case 's3':
      return !!cfg.endpoint && !!cfg.bucket && !!cfg.access_key;
  }
  return false;
}

export function stepValid(step: number, name: string, type: TargetType, cfg: TargetConfig): boolean {
  if (step === 0) return name.trim().length > 0;
  if (step === 1) return connectionValid(type, cfg);
  return true;
}
