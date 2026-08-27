import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "@copilotkit/react-core/v2/styles.css";
import "./styles.css";
import { App } from "./app";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
