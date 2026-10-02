package oauth

import "strings"

const oauthLogoSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 800 800" aria-hidden="true"><path fill="#F09082" d="M165.29 165.29H517.36V400H400V282.65H165.29Z"/><path fill="#4D9ABF" d="M165.29 282.65H282.65V400H400V517.36H282.65V634.72H165.29Z"/><path fill="#F1BE58" d="M517.36 400H634.72V634.72H517.36Z"/></svg>`

const oauthPageTemplate = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>{{TITLE}}</title>
  <style>
    :root {
      --text: #fafafa;
      --text-dim: #a1a1aa;
      --page-bg: #09090b;
      --font-sans: ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, "Noto Sans", sans-serif, "Apple Color Emoji", "Segoe UI Emoji", "Segoe UI Symbol", "Noto Color Emoji";
      --font-mono: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", "Courier New", monospace;
    }
    * { box-sizing: border-box; }
    html { color-scheme: dark; }
    body {
      margin: 0;
      min-height: 100vh;
      display: flex;
      align-items: center;
      justify-content: center;
      padding: 24px;
      background: var(--page-bg);
      color: var(--text);
      font-family: var(--font-sans);
      text-align: center;
    }
    main {
      width: 100%;
      max-width: 560px;
      display: flex;
      flex-direction: column;
      align-items: center;
      justify-content: center;
    }
    .logo {
      width: 72px;
      height: 72px;
      display: block;
      margin-bottom: 24px;
    }
    h1 {
      margin: 0 0 10px;
      font-size: 28px;
      line-height: 1.15;
      font-weight: 650;
      color: var(--text);
    }
    p {
      margin: 0;
      line-height: 1.7;
      color: var(--text-dim);
      font-size: 15px;
    }
    .details {
      margin-top: 16px;
      font-family: var(--font-mono);
      font-size: 13px;
      color: var(--text-dim);
      white-space: pre-wrap;
      word-break: break-word;
    }
  </style>
</head>
<body>
  <main>
    <div class="logo">{{LOGO}}</div>
    <h1>{{HEADING}}</h1>
    <p>{{MESSAGE}}</p>
    {{DETAILS}}
  </main>
</body>
</html>`

// orbPageTemplate is the browser page of Orb's own sign-in callbacks: Ubuntu
// Mono on the Ordalie palette, one stretched-caps title, red only on failure.
const orbPageTemplate = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>{{TITLE}}</title>
  <style>
    :root { --bg: #12151A; --text: #FAF9F6; --dim: #6A6F77; --accent: #C94A3D; color-scheme: dark; }
    @media (prefers-color-scheme: light) { :root { --bg: #FAF9F6; --text: #12151A; --dim: #8C8C87; color-scheme: light; } }
    * { box-sizing: border-box; }
    body {
      margin: 0; min-height: 100vh; display: flex; align-items: center; padding: 32px 16px;
      background: var(--bg); color: var(--text); font: 13px/1.6 "Ubuntu Mono", ui-monospace, SFMono-Regular, Menlo, monospace;
    }
    main { width: 100%; max-width: 560px; margin: 0 auto; }
    .mark { font-size: 10px; letter-spacing: 2px; color: var(--dim); }
    h1 {
      margin: 28px 0 36px; font-size: 40px; font-weight: 700; line-height: 1; letter-spacing: 1px;
      transform: scaleY(1.9); transform-origin: top left;
    }
    .failed h1 { color: var(--accent); }
    p { margin: 0; }
    .details { margin-top: 12px; color: var(--dim); white-space: pre-wrap; word-break: break-word; }
    .next { margin-top: 24px; padding-top: 12px; border-top: 1px solid var(--dim); color: var(--dim); }
  </style>
</head>
<body>
  <main class="{{CLASS}}">
    <div class="mark">ORB</div>
    <h1>{{HEADING}}</h1>
    <p>{{MESSAGE}}</p>
    {{DETAILS}}
    <p class="next">{{NEXT}}</p>
  </main>
</body>
</html>`

func successPage(message string) string {
	return renderOrbPage("Connected · Orb", "CONNECTED", message, "", "You can close this page and return to Orb.", "")
}

func errorPage(message string) string {
	return errorPageWithDetails(message, "")
}

func errorPageWithDetails(message, details string) string {
	return renderOrbPage("Not connected · Orb", "NOT CONNECTED", message, details, "Return to Orb to try again.", "failed")
}

func renderOrbPage(title, heading, message, details, next, class string) string {
	detailsHTML := ""
	if details != "" {
		detailsHTML = `<p class="details">` + escapeOAuthHTML(details) + `</p>`
	}
	return strings.NewReplacer(
		"{{TITLE}}", escapeOAuthHTML(title),
		"{{CLASS}}", class,
		"{{HEADING}}", escapeOAuthHTML(heading),
		"{{MESSAGE}}", escapeOAuthHTML(message),
		"{{DETAILS}}", detailsHTML,
		"{{NEXT}}", escapeOAuthHTML(next),
	).Replace(orbPageTemplate)
}

// OAuthSuccessHTML retains the upstream HTML helper contract; browser callbacks
// use the Orb presentation above.
func OAuthSuccessHTML(message string) string {
	return renderOAuthPage("Authentication successful", "Authentication successful", message, "", oauthLogoSVG)
}

func OAuthErrorHTML(message, details string) string {
	return renderOAuthPage("Authentication failed", "Authentication failed", message, details, oauthLogoSVG)
}

func renderOAuthPage(title, heading, message, details, logo string) string {
	detailsHTML := ""
	if details != "" {
		detailsHTML = `<div class="details">` + escapeOAuthHTML(details) + `</div>`
	}
	replacer := strings.NewReplacer(
		"{{TITLE}}", escapeOAuthHTML(title),
		"{{LOGO}}", logo,
		"{{HEADING}}", escapeOAuthHTML(heading),
		"{{MESSAGE}}", escapeOAuthHTML(message),
		"{{DETAILS}}", detailsHTML,
	)
	return replacer.Replace(oauthPageTemplate)
}

func escapeOAuthHTML(value string) string {
	return strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&#39;",
	).Replace(value)
}
