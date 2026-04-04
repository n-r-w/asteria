type Notification = {
    message: string;
};

function buildNotification(message: string): Notification {
    return { message };
}

export function execute(mode: "left" | "right"): string {
    if (mode === "left") {
        const notification = buildNotification("left");
        return notification.message;
    }

    const notification = buildNotification("right");
    return notification.message;
}
