struct fallback_payload {
    int value;
};

int fallback_c_entry(void) {
    struct fallback_payload payload = {4};

    return payload.value;
}
