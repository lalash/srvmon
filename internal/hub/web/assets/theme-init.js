/* Runs before first paint so a dark-mode reload never flashes white. */
(function () {
  if (localStorage.getItem('srvmon-dark') !== 'false') {
    document.documentElement.classList.add('pre-dark');
  }
  document.addEventListener('DOMContentLoaded', function () {
    if (document.documentElement.classList.contains('pre-dark')) {
      document.body.classList.add('dark');
    }
    var lang = localStorage.getItem('srvmon-lang') || 'en';
    document.documentElement.lang = lang;
    document.documentElement.dir = lang === 'fa' ? 'rtl' : 'ltr';
  });
})();
