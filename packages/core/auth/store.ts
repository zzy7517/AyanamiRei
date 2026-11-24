import { create } from "zustand";
import type { User, StorageAdapter } from "../types";
import { identify as identifyAnalytics } from "../analytics";
import type { ApiClient } from "../api/client";

export interface AuthStoreOptions {
  api: ApiClient;
  storage: StorageAdapter;
}

export type AuthStatus = "authenticated" | "recovering";

export interface AuthState {
  user: User | null;
  isLoading: boolean;
  status: AuthStatus;
  retryGeneration: number;
  retryAuthentication: () => void;
  setUser: (user: User) => void;
  refreshMe: () => Promise<void>;
}

export function createAuthStore(options: AuthStoreOptions) {
  const { api } = options;

  return create<AuthState>((set) => ({
    user: null,
    isLoading: true,
    status: "recovering",
    retryGeneration: 0,
    retryAuthentication: () => {
      set((state) => ({
        isLoading: true,
        status: "recovering",
        retryGeneration: state.retryGeneration + 1,
      }));
    },
    setUser: (user) => {
      identifyAnalytics(user.id, { email: user.email, name: user.name });
      set({ user, isLoading: false, status: "authenticated" });
    },
    refreshMe: async () => {
      const user = await api.getMe();
      identifyAnalytics(user.id, { email: user.email, name: user.name });
      set({ user, isLoading: false, status: "authenticated" });
    },
  }));
}
