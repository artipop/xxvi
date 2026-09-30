import { render } from "@solidjs/web";
import App from "./App";
import "./styles.css";
import "./theme";

const root = document.getElementById("root");
if (!root) throw new Error("no root element");
render(() => <App />, root);
