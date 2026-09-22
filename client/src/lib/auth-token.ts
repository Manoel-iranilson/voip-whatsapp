const KEY = "wacalls.token";

/**
 * The API token, when the server was started with -token.
 *
 * The operator hands it over once by opening the app as `?token=...`; it is then
 * kept in localStorage and stripped from the address bar so it does not end up in
 * bookmarks or shoulder-surfed from the URL. Returns "" when the server runs
 * without a token, which is the default.
 */
export const getAuthToken = (): string => {
  try {
    const fromUrl = new URLSearchParams(window.location.search).get("token");
    if (fromUrl) {
      localStorage.setItem(KEY, fromUrl);
      const url = new URL(window.location.href);
      url.searchParams.delete("token");
      window.history.replaceState({}, "", url.toString());
      return fromUrl;
    }
    return localStorage.getItem(KEY) ?? "";
  } catch {
    return "";
  }
};
