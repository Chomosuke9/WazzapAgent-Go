import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import { AuthGate } from "./hooks/AuthGate";
import { AppProvider } from "./hooks/AppProvider";
import "./styles/app.css";

createRoot(document.getElementById("root")!).render(<StrictMode><AuthGate><AppProvider><App /></AppProvider></AuthGate></StrictMode>);
