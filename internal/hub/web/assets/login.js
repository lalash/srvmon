import { t, applyDirection } from './i18n.js';

applyDirection();

document.getElementById('usernameLabel').textContent = t('username');
document.getElementById('passwordLabel').textContent = t('password');
document.getElementById('submit').textContent = t('signIn');

const form = document.getElementById('loginForm');
const errorBox = document.getElementById('error');

form.addEventListener('submit', async (event) => {
  event.preventDefault();
  errorBox.textContent = '';

  const submit = document.getElementById('submit');
  submit.disabled = true;
  try {
    const response = await fetch('/api/auth/login', {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        username: document.getElementById('username').value.trim(),
        password: document.getElementById('password').value,
      }),
    });
    const data = await response.json().catch(() => null);
    if (!response.ok) throw new Error((data && data.error) || response.statusText);
    window.location.href = '/';
  } catch (error) {
    errorBox.textContent = error.message;
    submit.disabled = false;
  }
});
