import { JS_STAMP } from "./fixture.js";

export default function JsxBadge({ label = JS_STAMP }) {
    return <badge>{String(label).trim()}</badge>;
}
