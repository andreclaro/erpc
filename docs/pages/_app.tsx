import type { AppProps } from "next/app";
import "../styles/components.css";
import "../styles/hero-diagram.css";
import "../styles/explainer.css";
import "../styles/deep-dive.css";
import "../styles/dd-finality.css";

export default function App({ Component, pageProps }: AppProps) {
	return <Component {...pageProps} />;
}
