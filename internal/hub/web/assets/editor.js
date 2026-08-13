import { icons } from './icons.js';

// A small rich-text editor over contenteditable. execCommand is deprecated but
// still the only thing every browser implements for this; the alternative is a
// selection-and-range engine, which is not worth it for ten formatting buttons.
// Anything it produces is sanitized server-side before it is stored.

const GROUPS = [
  [
    { cmd: 'bold', label: 'B', title: 'bold', style: 'font-weight:700' },
    { cmd: 'italic', label: 'I', title: 'italic', style: 'font-style:italic' },
    { cmd: 'underline', label: 'U', title: 'underline', style: 'text-decoration:underline' },
    { cmd: 'strikeThrough', label: 'S', title: 'strikethrough', style: 'text-decoration:line-through' },
  ],
  [
    { block: 'h2', label: 'H2', title: 'heading' },
    { block: 'h3', label: 'H3', title: 'subheading' },
    { block: 'p', label: '¶', title: 'paragraph' },
  ],
  [
    { cmd: 'insertUnorderedList', icon: 'listUl', title: 'bulleted list' },
    { cmd: 'insertOrderedList', icon: 'listOl', title: 'numbered list' },
    { block: 'blockquote', icon: 'quote', title: 'quote' },
  ],
  [
    { action: 'code', icon: 'codeTag', title: 'inline code' },
    { action: 'link', icon: 'linkIcon', title: 'link' },
    { cmd: 'insertHorizontalRule', icon: 'rule', title: 'divider' },
  ],
  [
    { action: 'rtl', icon: 'dirRtl', title: 'right to left' },
    { action: 'ltr', icon: 'dirLtr', title: 'left to right' },
    { cmd: 'removeFormat', icon: 'eraser', title: 'clear formatting' },
  ],
];

export function createEditor(host, options = {}) {
  const onChange = options.onChange || (() => {});

  host.innerHTML = `
    <div class="ed">
      <div class="ed-bar" role="toolbar"></div>
      <div class="ed-body" contenteditable="true" dir="auto" spellcheck="true"
           role="textbox" aria-multiline="true"
           data-placeholder="${(options.placeholder || '').replace(/"/g, '&quot;')}"></div>
    </div>`;

  const bar = host.querySelector('.ed-bar');
  const body = host.querySelector('.ed-body');

  GROUPS.forEach((group, index) => {
    if (index > 0) bar.appendChild(divider());
    for (const item of group) bar.appendChild(button(item, body, onChange));
  });

  body.addEventListener('input', onChange);
  // Paste as plain text: the sanitizer would strip a word processor's markup
  // anyway, and doing it here means what you see pasted is what gets saved.
  body.addEventListener('paste', (event) => {
    event.preventDefault();
    const text = (event.clipboardData || window.clipboardData).getData('text/plain');
    document.execCommand('insertText', false, text);
  });

  return {
    get html() {
      return body.innerHTML;
    },
    set html(value) {
      body.innerHTML = value || '';
    },
    focus() {
      body.focus();
    },
    get isEmpty() {
      return body.textContent.trim() === '' && !body.querySelector('img,hr');
    },
    onBlur(handler) {
      body.addEventListener('blur', handler);
    },
  };
}

function divider() {
  const span = document.createElement('span');
  span.className = 'ed-sep';
  return span;
}

function button(item, body, onChange) {
  const el = document.createElement('button');
  el.type = 'button';
  el.className = 'ed-btn';
  el.title = item.title;
  el.setAttribute('aria-label', item.title);
  if (item.icon) {
    el.innerHTML = icons[item.icon];
    el.classList.add('ed-btn-icon');
  } else {
    el.textContent = item.label;
  }
  if (item.style) el.setAttribute('style', item.style);

  // mousedown, not click: the default would move focus out of the editor and
  // collapse the selection before the command could apply to it.
  el.addEventListener('mousedown', (event) => {
    event.preventDefault();
    body.focus();
    apply(item, body);
    onChange();
  });
  return el;
}

function apply(item, body) {
  if (item.cmd) {
    document.execCommand(item.cmd, false, null);
    return;
  }
  if (item.block) {
    document.execCommand('formatBlock', false, item.block);
    return;
  }
  switch (item.action) {
    case 'code': return wrapInline('code');
    case 'link': return insertLink();
    case 'rtl': return setDirection(body, 'rtl');
    case 'ltr': return setDirection(body, 'ltr');
    default:
  }
}

function wrapInline(tag) {
  const selection = window.getSelection();
  if (!selection.rangeCount || selection.isCollapsed) return;

  const range = selection.getRangeAt(0);
  const wrapper = document.createElement(tag);
  try {
    range.surroundContents(wrapper);
  } catch {
    // surroundContents refuses a selection that crosses element boundaries;
    // extracting and re-inserting handles that case.
    wrapper.appendChild(range.extractContents());
    range.insertNode(wrapper);
  }
  selection.removeAllRanges();
  const after = document.createRange();
  after.selectNodeContents(wrapper);
  selection.addRange(after);
}

function insertLink() {
  const url = window.prompt('Link address', 'https://');
  if (!url) return;
  const selection = window.getSelection();
  if (selection && selection.isCollapsed) {
    document.execCommand('insertHTML', false, `<a href="${escapeAttr(url)}">${escapeText(url)}</a>`);
    return;
  }
  document.execCommand('createLink', false, url);
}

// Direction is set on the block the caret sits in, not the whole editor, so a
// Persian paragraph and an English command block can sit side by side.
function setDirection(body, dir) {
  const selection = window.getSelection();
  if (!selection.rangeCount) return;

  let node = selection.getRangeAt(0).startContainer;
  if (node.nodeType === Node.TEXT_NODE) node = node.parentNode;
  while (node && node !== body && !isBlock(node)) node = node.parentNode;

  if (!node || node === body) {
    // No block yet — wrap the line so the direction has something to live on.
    document.execCommand('formatBlock', false, 'p');
    node = selection.getRangeAt(0).startContainer;
    if (node.nodeType === Node.TEXT_NODE) node = node.parentNode;
    while (node && node !== body && !isBlock(node)) node = node.parentNode;
  }
  if (node && node !== body) node.setAttribute('dir', dir);
}

function isBlock(node) {
  return ['P', 'DIV', 'H2', 'H3', 'H4', 'LI', 'BLOCKQUOTE', 'PRE'].includes(node.nodeName);
}

function escapeAttr(value) {
  return String(value).replace(/"/g, '&quot;').replace(/</g, '&lt;');
}

function escapeText(value) {
  return String(value).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}

export const editorIcons = icons;
