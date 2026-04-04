export const moduleStamp = "module-stamp";

export function mtsHelper(label: string): string {
    return `${moduleStamp}:${label}`;
}
