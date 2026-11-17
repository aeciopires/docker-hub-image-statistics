// Applies the stored theme before the first paint. Dark is the default.
(function () {
  var theme = "dark";
  try {
    var stored = window.localStorage.getItem("dhis-theme");
    if (stored === "light" || stored === "dark") theme = stored;
  } catch (e) { /* storage unavailable: keep the default */ }
  document.documentElement.setAttribute("data-theme", theme);
})();
