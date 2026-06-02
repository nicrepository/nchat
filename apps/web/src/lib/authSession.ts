const ACCESS_TOKEN_KEY = "nchat_at";
const REFRESH_TOKEN_KEY = "nchat_rt";

export function setTokens(accessToken: string, refreshToken: string): void {
  sessionStorage.setItem(ACCESS_TOKEN_KEY, accessToken);
  sessionStorage.setItem(REFRESH_TOKEN_KEY, refreshToken);
}

export function getAccessToken(): string | null {
  return sessionStorage.getItem(ACCESS_TOKEN_KEY);
}

export function getRefreshToken(): string | null {
  return sessionStorage.getItem(REFRESH_TOKEN_KEY);
}

export function clearTokens(): void {
  sessionStorage.removeItem(ACCESS_TOKEN_KEY);
  sessionStorage.removeItem(REFRESH_TOKEN_KEY);
}

export function isAuthenticated(): boolean {
  return (
    sessionStorage.getItem(ACCESS_TOKEN_KEY) !== null ||
    sessionStorage.getItem(REFRESH_TOKEN_KEY) !== null
  );
}
