#pragma once

#include <string>

namespace demo {

enum class WidgetKind {
    Basic,
    Fancy,
};

union Payload {
    int integer;
    double floating;
};

struct Box {
    int width;
    int height;
};

template <typename T>
T clamp_value(T value, T min_value, T max_value) {
    if (value < min_value) {
        return min_value;
    }
    if (value > max_value) {
        return max_value;
    }

    return value;
}

class Identifiable {
public:
    virtual ~Identifiable() = default;
    virtual std::string id() const = 0;
};

class Widget final : public Identifiable {
public:
    using SizeType = int;
    static constexpr int default_scale = 2;

    Widget(std::string name, WidgetKind kind);
    ~Widget() override;

    std::string id() const override;
    int scale(int factor) const;
    static Widget create(std::string name);

private:
    std::string name_;
    WidgetKind kind_;
};

Box make_box(int width, int height);
int operate(int value);
int operate(int left, int right);

} // namespace demo
