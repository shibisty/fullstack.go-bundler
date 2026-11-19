// @fullstack/bundler: registers the service worker (Options.PWA).
if ("serviceWorker" in navigator) {
  navigator.serviceWorker.register("/sw.js", { scope: "/" }).catch((err) => {
    console.warn("fullstack: the service worker did not register:", err);
  });
}

// fullstackPWA.clear() forgets the pages and data kept for offline use,
// e.g. when the user logs out.
window.fullstackPWA = {
  clear() {
    const sw = navigator.serviceWorker && navigator.serviceWorker.controller;
    if (sw) sw.postMessage("clear");
  },
};
