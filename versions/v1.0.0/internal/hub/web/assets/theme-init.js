/* Runs before first paint so a dark-mode reload never flashes white. The class
   goes on <html>, not <body>: the theme tokens and the :root aliases built from
   them have to be declared on the same element or the aliases keep the values
   of whichever theme :root happens to hold. */
(function () {
  if (localStorage.getItem('srvmon-dark') !== 'false') {
    document.documentElement.classList.add('dark');
  }
  var lang = localStorage.getItem('srvmon-lang') || 'en';
  document.documentElement.lang = lang;
  document.documentElement.dir = lang === 'fa' ? 'rtl' : 'ltr';
})();
