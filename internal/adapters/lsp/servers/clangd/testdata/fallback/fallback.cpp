class FallbackWidget {
public:
    int double_value(int value) const;
};

int FallbackWidget::double_value(int value) const {
    return value * 2;
}

int fallback_cpp_entry() {
    FallbackWidget widget;

    return widget.double_value(3);
}
