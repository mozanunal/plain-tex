// Behaviors for the server-rendered pages (projects, settings, admin). They are
// wired through data attributes instead of inline handlers, because the
// Content-Security-Policy forbids inline script.

document.addEventListener('submit', (event) => {
    const form = event.target.closest('form[data-confirm]');
    if (form && !window.confirm(form.dataset.confirm)) {
        event.preventDefault();
    }
});

document.addEventListener('click', (event) => {
    const button = event.target.closest('button[data-action]');
    if (!button) {
        return;
    }
    switch (button.dataset.action) {
        case 'generate-password':
            fillGeneratedPassword(button);
            break;
        case 'copy-public-key':
            copyPublicKey(button);
            break;
        case 'delete-project':
            deleteProject(button);
            break;
    }
});

function fillGeneratedPassword(button) {
    const chars = 'ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789';
    const values = new Uint32Array(16);
    crypto.getRandomValues(values);
    let password = '';
    for (const value of values) {
        password += chars[value % chars.length];
    }
    const input = button.closest('form').querySelector('input[name="password"]');
    input.type = 'text';
    input.value = password;
    input.focus();
    input.select();
}

function copyPublicKey(button) {
    const textarea = button.closest('.space-y-2').querySelector('textarea');
    navigator.clipboard.writeText(textarea.value.trim())
        .then(() => {
            button.textContent = 'Copied!';
            setTimeout(() => { button.textContent = 'Copy'; }, 2000);
        })
        .catch(() => { button.textContent = 'Failed'; });
}

async function deleteProject(button) {
    if (!window.confirm('Delete this project?')) {
        return;
    }
    const response = await fetch(button.dataset.url, { method: 'DELETE' });
    if (!response.ok) {
        window.alert('Failed to delete the project.');
        return;
    }
    button.closest('[data-project-card]').remove();
}
