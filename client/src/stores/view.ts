import { create } from "zustand";

export type View = "sessions" | "calls" | "history";

export const useView = create<{ view: View }>(() => ({ view: "sessions" }));

export const setView = (view: View): void => useView.setState({ view });
