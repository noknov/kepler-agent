import React, { createContext, useContext } from "react";

type AppState = {
  isBriefOnly: boolean;
  viewingAgentTaskId: string | null;
};

const defaultState: AppState = {
  isBriefOnly: false,
  viewingAgentTaskId: null,
};

const AppStateContext = createContext<AppState>(defaultState);

export function useAppState<T>(selector: (state: AppState) => T): T {
  return selector(useContext(AppStateContext));
}

export function useAppStateMaybeOutsideOfProvider<T>(
  selector: (state: AppState) => T,
): T {
  return selector(useContext(AppStateContext));
}

export function useAppStateStore<T>(selector: (state: AppState) => T): T {
  return selector(useContext(AppStateContext));
}

export function AppStateProvider({ children }: { children: React.ReactNode }) {
  return <AppStateContext.Provider value={defaultState}>{children}</AppStateContext.Provider>;
}
