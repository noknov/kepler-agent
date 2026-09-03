export function getFeatureValue_CACHED_MAY_BE_STALE<T>(_key: string, fallback: T): T {
  return fallback;
}

export function checkStatsigFeatureGate_CACHED_MAY_BE_STALE(_key: string, fallback: boolean): boolean {
  return fallback;
}

export function logEvent(): void {}

export type AnalyticsMetadata_I_VERIFIED_THIS_IS_NOT_CODE_OR_FILEPATHS = Record<string, unknown>;
