import { mount } from 'svelte';
import App from './App.svelte';
import './style.css';
const raw = document.getElementById('bootstrap')?.textContent;
if (!raw) throw new Error('Missing page data');
mount(App, { target: document.getElementById('app')!, props: { bootstrap: JSON.parse(raw) } });
