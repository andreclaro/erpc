import type { AppProps } from "next/app";
import "../styles/components.css";
import "../styles/hero-diagram.css";
import "../styles/explainer.css";
import "../styles/deep-dive.css";
import "../styles/dd-finality.css";
import "../styles/dd-svm.css";
import "../styles/dd-errors.css";
import "../styles/dd-metrics.css";

export default function App({ Component, pageProps }: AppProps) {
	return <Component {...pageProps} />;
}
