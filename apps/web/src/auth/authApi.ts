import { apiFetch } from "../lib/api";

const AUTH_BASE = import.meta.env.VITE_AUTH_API_BASE_URL ?? "/api/auth";

export interface LoginInput {
  email: string;
  password: string;
  deviceName?: string;
}

export interface TokenPair {
  accessToken: string;
  refreshToken: string;
  tokenType: string;
  expiresIn: number;
}

export interface LoginResponse extends TokenPair {
  user: {
    id: string;
    email: string;
    displayName: string;
    mustChangePassword: boolean;
  };
}

export interface AcceptInviteInput {
  token: string;
  displayName: string;
  fullName?: string;
  password: string;
}

export interface AcceptInviteResponse {
  id: string;
  email: string;
  displayName: string;
  fullName?: string;
  createdAt: string;
}

type RawLoginResponse = {
  access_token: string;
  refresh_token: string;
  token_type: string;
  expires_in: number;
  user: {
    id: string;
    email: string;
    display_name: string;
    must_change_password: boolean;
  };
};

type RawTokenResponse = {
  access_token: string;
  refresh_token: string;
  token_type: string;
  expires_in: number;
};

type RawAcceptInviteResponse = {
  id: string;
  email: string;
  display_name: string;
  full_name?: string;
  created_at: string;
};

function mapTokenPair(raw: RawTokenResponse): TokenPair {
  return {
    accessToken: raw.access_token,
    refreshToken: raw.refresh_token,
    tokenType: raw.token_type,
    expiresIn: raw.expires_in,
  };
}

export async function login(input: LoginInput): Promise<LoginResponse> {
  const raw = await apiFetch<RawLoginResponse>(`${AUTH_BASE}/login`, {
    method: "POST",
    body: JSON.stringify({
      email: input.email,
      password: input.password,
      device_name: input.deviceName,
    }),
  });

  return {
    ...mapTokenPair(raw),
    user: {
      id: raw.user.id,
      email: raw.user.email,
      displayName: raw.user.display_name,
      mustChangePassword: raw.user.must_change_password,
    },
  };
}

export async function refresh(refreshToken: string): Promise<TokenPair> {
  const raw = await apiFetch<RawTokenResponse>(`${AUTH_BASE}/refresh`, {
    method: "POST",
    body: JSON.stringify({ refresh_token: refreshToken }),
  });

  return mapTokenPair(raw);
}

export async function logout(refreshToken: string): Promise<void> {
  await apiFetch<void>(`${AUTH_BASE}/logout`, {
    method: "POST",
    body: JSON.stringify({ refresh_token: refreshToken }),
  });
}

export async function forgotPassword(email: string): Promise<void> {
  await apiFetch<void>(`${AUTH_BASE}/password/forgot`, {
    method: "POST",
    body: JSON.stringify({ email }),
  });
}

export async function resetPassword(token: string, newPassword: string): Promise<void> {
  await apiFetch<void>(`${AUTH_BASE}/password/reset`, {
    method: "POST",
    body: JSON.stringify({ token, new_password: newPassword }),
  });
}

export async function acceptInvite(input: AcceptInviteInput): Promise<AcceptInviteResponse> {
  const raw = await apiFetch<RawAcceptInviteResponse>(`${AUTH_BASE}/invites/accept`, {
    method: "POST",
    body: JSON.stringify({
      token: input.token,
      display_name: input.displayName,
      full_name: input.fullName,
      password: input.password,
    }),
  });

  return {
    id: raw.id,
    email: raw.email,
    displayName: raw.display_name,
    fullName: raw.full_name,
    createdAt: raw.created_at,
  };
}
