import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { I18nProvider } from "react-aria-components";
import { App } from "./App";
import "./app.css";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <I18nProvider locale="en-GB">
      <App />
    </I18nProvider>
  </StrictMode>,
);
