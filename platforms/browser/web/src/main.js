import { mount } from "svelte";
import "./styles/tokens.css";
import App from "./App.svelte";

mount(App, {
  target: document.getElementById("root"),
});
