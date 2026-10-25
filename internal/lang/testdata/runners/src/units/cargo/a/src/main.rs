fn main() {
    println!("{}", units_a::add(1, 2));
}

#[cfg(test)]
mod tests {
    #[test]
    fn runs() {
        assert_eq!(units_a::add(0, 1), 1);
    }
}
